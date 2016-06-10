package engine

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/concourse/atc"
	"github.com/concourse/atc/db"
	"github.com/concourse/atc/metric"
	"github.com/pivotal-golang/lager"
)

var ErrBuildNotActive = errors.New("build not yet active")

const trackingInterval = 10 * time.Second

func NewDBEngine(engines Engines, buildDBFactory db.BuildDBFactory) Engine {
	return &dbEngine{
		engines:        engines,
		buildDBFactory: buildDBFactory,
	}
}

type UnknownEngineError struct {
	Engine string
}

func (err UnknownEngineError) Error() string {
	return fmt.Sprintf("unknown build engine: %s", err.Engine)
}

type dbEngine struct {
	engines        Engines
	buildDBFactory db.BuildDBFactory
}

func (*dbEngine) Name() string {
	return "db"
}

func (engine *dbEngine) CreateBuild(logger lager.Logger, buildDB db.BuildDB, plan atc.Plan) (Build, error) {
	buildEngine := engine.engines[0]

	createdBuild, err := buildEngine.CreateBuild(logger, buildDB, plan)
	if err != nil {
		return nil, err
	}

	started, err := buildDB.Start(buildEngine.Name(), createdBuild.Metadata())
	if err != nil {
		return nil, err
	}

	if !started {
		createdBuild.Abort(logger.Session("aborted-immediately"))
	}

	return &dbBuild{
		engines: engine.engines,
		buildDB: buildDB,
	}, nil
}

func (engine *dbEngine) LookupBuild(logger lager.Logger, buildDB db.BuildDB) (Build, error) {
	return &dbBuild{
		engines: engine.engines,
		buildDB: buildDB,
	}, nil
}

type dbBuild struct {
	engines Engines
	buildDB db.BuildDB
}

func (build *dbBuild) Metadata() string {
	return strconv.Itoa(build.buildDB.GetID())
}

func (build *dbBuild) PublicPlan(logger lager.Logger) (atc.PublicBuildPlan, error) {
	buildEngineName := build.buildDB.GetEngine()
	buildEngine, found := build.engines.Lookup(buildEngineName)
	if !found {
		logger.Error("unknown-engine", nil, lager.Data{"engine": buildEngineName})
		return atc.PublicBuildPlan{}, UnknownEngineError{buildEngineName}
	}

	engineBuild, err := buildEngine.LookupBuild(logger, build.buildDB)
	if err != nil {
		return atc.PublicBuildPlan{}, err
	}

	return engineBuild.PublicPlan(logger)
}

func (build *dbBuild) Abort(logger lager.Logger) error {
	// the order below is very important to avoid races with build creation.

	lease, leased, err := build.buildDB.LeaseTracking(logger, trackingInterval)
	if err != nil {
		logger.Error("failed-to-get-lease", err)
		return err
	}

	if !leased {
		// someone else is tracking the build; abort it, which will notify them
		logger.Info("notifying-other-tracker")
		return build.buildDB.Abort()
	}

	defer lease.Break()

	// no one is tracking the build; abort it ourselves

	// first save the status so that CreateBuild will see a conflict when it
	// tries to mark the build as started.
	err = build.buildDB.Abort()
	if err != nil {
		logger.Error("failed-to-abort-in-database", err)
		return err
	}

	// reload the model *after* saving the status for the following check to see
	// if it was already started
	found, err := build.buildDB.Reload()
	if err != nil {
		logger.Error("failed-to-get-build-from-database", err)
		return err
	}

	if !found {
		logger.Info("build-not-found")
		return nil
	}

	buildEngineName := build.buildDB.GetEngine()
	// if there's an engine, there's a real build to abort
	if buildEngineName == "" {
		// otherwise, CreateBuild had not yet tried to start the build, and so it
		// will see the conflict when it tries to transition, and abort itself.
		//
		// finish the build so that the aborted event is put into the event stream
		// even if the build has not started yet
		logger.Info("finishing-build-with-no-engine")
		return build.buildDB.Finish(db.StatusAborted)
	}

	buildEngine, found := build.engines.Lookup(buildEngineName)
	if !found {
		logger.Error("unknown-engine", nil, lager.Data{"engine": buildEngineName})
		return UnknownEngineError{buildEngineName}
	}

	// find the real build to abort...
	engineBuild, err := buildEngine.LookupBuild(logger, build.buildDB)
	if err != nil {
		logger.Error("failed-to-lookup-build-in-engine", err)
		return err
	}

	// ...and abort it.
	return engineBuild.Abort(logger)
}

func (build *dbBuild) Resume(logger lager.Logger) {
	lease, leased, err := build.buildDB.LeaseTracking(logger, trackingInterval)
	if err != nil {
		logger.Error("failed-to-get-lease", err)
		return
	}

	if !leased {
		logger.Debug("build-already-tracked")
		return
	}

	defer lease.Break()

	found, err := build.buildDB.Reload()
	if err != nil {
		logger.Error("failed-to-load-build-from-db", err)
		return
	}

	if !found {
		logger.Info("build-not-found")
		return
	}

	buildEngineName := build.buildDB.GetEngine()
	if buildEngineName == "" {
		logger.Error("build-has-no-engine", err)
		return
	}

	if !build.buildDB.IsRunning() {
		logger.Info("build-already-finished", lager.Data{
			"build-id": build.buildDB.GetID(),
		})
		return
	}

	buildEngine, found := build.engines.Lookup(buildEngineName)
	if !found {
		logger.Error("unknown-build-engine", nil, lager.Data{
			"engine": buildEngineName,
		})
		build.finishWithError(logger)
		return
	}

	engineBuild, err := buildEngine.LookupBuild(logger, build.buildDB)
	if err != nil {
		logger.Error("failed-to-lookup-build-from-engine", err)
		build.finishWithError(logger)
		return
	}

	aborts, err := build.buildDB.AbortNotifier()
	if err != nil {
		logger.Error("failed-to-listen-for-aborts", err)
		return
	}

	defer aborts.Close()

	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-aborts.Notify():
			logger.Info("aborting")

			err := engineBuild.Abort(logger)
			if err != nil {
				logger.Error("failed-to-abort", err)
			}
		case <-done:
		}
	}()

	metric.BuildStarted{
		PipelineName: build.buildDB.GetPipelineName(),
		JobName:      build.buildDB.GetJobName(),
		BuildName:    build.buildDB.GetName(),
		BuildID:      build.buildDB.GetID(),
	}.Emit(logger)

	logger.Info("running")
	engineBuild.Resume(logger)

	found, err = build.buildDB.Reload()
	if err != nil {
		logger.Error("failed-to-load-build-from-db", err)
		return
	}

	if !found {
		logger.Info("build-removed")
		return
	}

	metric.BuildFinished{
		PipelineName:  build.buildDB.GetPipelineName(),
		JobName:       build.buildDB.GetJobName(),
		BuildName:     build.buildDB.GetName(),
		BuildID:       build.buildDB.GetID(),
		BuildStatus:   build.buildDB.GetStatus(),
		BuildDuration: build.buildDB.GetEndTime().Sub(build.buildDB.GetStartTime()),
	}.Emit(logger)
}

func (build *dbBuild) finishWithError(logger lager.Logger) {
	err := build.buildDB.Finish(db.StatusErrored)
	if err != nil {
		logger.Error("failed-to-mark-build-as-errored", err)
	}
}

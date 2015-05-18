#lang concourse/docs

@title[#:style '(quiet unnumbered)]{v0.44.0}

This release is hella backwards incompatible. Read carefully, and ask in IRC
(@hyperlink["irc://irc.freenode.net/concourse"]{#concourse}) if you need help!

We won't be making such drastic changes after 1.0, but as long as we're still
figuring things out, we don't want to collect tech debt or land on the wrong
set of primitives.

@itemlist[
  @item{
    @emph{Backwards-incompatible:} the progression of artifacts through
    a build plan has been made more explicit.

    Previously there was basically a working directory that would be streamed
    from step to step, and @code{aggregate} steps were relied on to place
    things under subdirectories, which is how inputs to tasks were satisfied.

    Now, as a plan executes, each step's produced artifact (for example
    a @code{get} step's fetched bits or the result of a @code{task}'s
    execution) are stored in a pool, with the source named after the step.

    This change affects many things, but the primary things you'll notice are
    as follows:

    @itemlist[
      @item{
        When executing a @code{task} step, its inputs are collected from the
        pool, rather than blindly streamed from the previous step. This means
        @code{aggregate} is no longer required to satisfy task inputs, and
        can now be removed if it's only wrapping one step.

        Tasks are now @emph{required} to list their set of inputs, otherwise
        no inputs will be streamed in. This is backwards-incompatible, but has
        many advantages: it's more explicit, more efficient, and makes it
        clearer where the dependent inputs will be placed in a task's working
        directory when it runs.

        When a task completes, its resulting working directory is added to the
        pool, named after the task itself. This is how you would @code{put}
        using artifacts generated by tasks.
      }

      @item{
        The @code{file} attribute of a @code{task} step must now qualify the
        path with the name of the source providing the file.
      }

      @item{
        When executing a @code{put} step, @emph{all} sources are fetched from
        the pool. Later on we may introduce a change so that @code{put} steps
        declare their dependencies, but for now streaming everything in is the
        simplest path forward.

        The net effect of this is that any params referring to files in
        @code{put} steps must now qualify the path with the source name, as
        they're all fetched into subdirectories.
      }

      @item{
        Now that there's a flat pool of sources, later steps in a build plan
        can now refer back to previously fetched (or generated) sources,
        rather than having to fetch them again.
      }
    ]

    So, if before you had a plan that looked like this:

    @codeblock["yaml"]|{
    plan:
    - aggregate:
      - get: something
    - task: generate-foo
      file: build.yml
    - put: foo-bucket
      params:
        from: foo
    }|

    ...it would now look like this:

    @codeblock["yaml"]|{
    plan:
    - get: something
    - task: generate-foo
      file: something/build.yml
    - put: foo-bucket
      params:
        from: generate-foo/foo
    }|

    Notably, the redundant @code{aggregate} is gone, the @code{file} attribute
    of the @code{task} step qualifies the filename with the name of the source
    containing it, and the @code{put} step qualifies the path to @code{foo}
    with the name of the task that it came from.

    Also, the @code{something/build.yml} task would now explicitly list its
    inputs, if it wasn't before. So that could mean changing:

    @codeblock["yaml"]|{
    platform: linux

    image: docker:///busybox

    run:
      path: something/some-script
    }|

    ...to...

    @codeblock["yaml"]|{
    platform: linux

    image: docker:///busybox

    inputs:
    - name: something

    run:
      path: something/some-script
    }|

    This has the advantage of making the task config more self-documenting,
    and removes any doubt as to what inputs will be placed where when the
    task starts.

    Note that listing inputs in the task config is not @emph{new}, and if you
    were already listing them before the semantics hasn't changed. The only
    difference is that they're now required.
  }

  @item{
    @emph{Backwards-incompatible}: worker registration is now done over SSH,
    using a new component called the
    @hyperlink["https://github.com/concourse/tsa"]{TSA}.

    To upgrade, you'll have to change your manifest a bit:

    @itemlist[
      @item{
        On your workers, replace the @code{gate} job with @code{groundcrew}
        and remove the @code{gate} properties.
      }

      @item{
        The new @code{tsa} job template will have to be added somewhere, and
        configured with the @code{atc} credentials (the same way @code{gate}
        used to be configured).

        Colocating @code{tsa} with the @code{atc} works out nicely, so that
        you can register its listening port @code{2222} with your routing
        layer (e.g. ELB), which will already be pointing at the ATC.
      }
    ]

    To compare, see the
    @hyperlink["https://github.com/concourse/concourse/blob/2f779277e112eef3ca94e3257395cc29ee70881d/manifests/aws-vpc.yml"]{example
    AWS VPC manifest}.

    The main upshot of this change is it's @emph{much} easier to securely
    register an external worker with Concourse. This new model only needs the
    worker to be able to reach the ATC rather than the other way around.
  }

  @item{
    @emph{Backwards-incompatible}: Consul services are now automatically
    registered based on the jobs being colocated with the agent. For this to
    work, you must edit your deployment manifest and move the
    @code{consul-agent} job to the top of each job template list, and remove
    your existing Consul services configuration from your manifest.
  }

  @item{
    The @code{get} and @code{put} steps from a build's execution can now be
    hijacked after they've finished or errored. Previously they would be
    reaped immediately; now they stick around for 5 minutes afterwards (same
    semantics as @code{task}s).
  }

  @item{
    The @hyperlink["https://github.com/concourse/s3-resource"]{S3 resource}
    now defaults to the @code{us-east-1} region.
  }

  @item{
    The @hyperlink["https://github.com/concourse/s3-resource"]{S3 resource}
    no longer fails to check when the configured bucket is empty.
  }

  @item{
    A new
    @hyperlink["https://github.com/concourse/bosh-deployment-resource"]{BOSH
    Deployment resource} has been introduced. It can be used to deploy a given
    set of release/stemcell tarballs with a manifest to a statically
    configured BOSH target. The precise versions of the releases and stemcells
    are overridden in the manifest before deploying to ensure it's not just
    always rolling forward to @code{latest}.
  }
]

# Accessing Downstream Builds

## Containers

For downstream container builds, we use [a private downstream build repository](https://github.com/flightctl/flightctl-build-downstream)
where all of the Konflux build pipeline lives. Check there to see details of the process
or do a release.

### Release Candidates
In general, release candidates (RC) builds will be published to repositories in the stage repository
with the following form:

`registry.stage.redhat.io/rhem/flightctl-<container name>-rhel9:vX.Y.Z-rc1`

You should be able to use your regular production credentials for the stage registry. If you get an
unauthorized error, confirm that the name of the repository and tag is correct, as nonexistant
repositories will be treated as unauthorized.

### Regular Releases
Regular releases will go to the production repository with the form:

`registry.redhat.io/rhem/flightctl-<container name>-rhel9:vX.Y.Z`

## RPMs

TODO

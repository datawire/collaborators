# collaborators.go

Print a summary of the permissions of all the repositories in the
a given GitHub organization.

Usage:

 1. Be an "owner" of the desired GitHub organization.
 2. Go to https://github.com/settings/tokens and create a Personal
    Access Token that has the `admin:org` and the `repo`(?)
    permission.
 3. `export GH_TOKEN=that_personal_access_token`
 4. `go run ./collaborators.go ORGNAME` Progress gets printed to
    stderr, actual output gets printed to stdout.  Be patient, it
    takes a couple minutes to run.

The output has most-recently-modified repos at the top, and repos that
haven't been modified in a long time at the bottom.

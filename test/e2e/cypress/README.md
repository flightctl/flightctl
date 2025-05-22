# Cypress testing 
## Installation

For installing the cypress testing tool is needed to have:

1. node.js installed
2. npm or yarn (npm preferred)
3. flightctl installed in openshift deployment (no local testing, only on cluster)

install with:
```
    cd /yourrepopath/flightctl/test/e2e/cypress/
    npm install cypress --save-dev
```

## Execution

To run the app in cypress folder you need to run:
```
  cd /yourrepopath/flightctl/test/e2e/cypress/
  export OPENSHIFT_PASSWORD="your user password"
  export OPENSHIFT_USERNAME="username"
  export OPENSHIFT_AUTH="openshift auth url"
  export OPENSHIFT_HOST="openshift console url"
  npx cypress open
```

To load on GUI mode.

If you want to load automaticly you need to run this command
```
  cd /yourrepopath/flightctl/test/e2e/cypress/
  export OPENSHIFT_PASSWORD="your user password"
  export OPENSHIFT_USERNAME="username"
  export OPENSHIFT_AUTH="openshift auth url"
  export OPENSHIFT_HOST="openshift console url"
  cypress run --browser chrome
```

This will run e2e testing on chrome browser for firefox use:
```
  cypress run --browser firefox
```
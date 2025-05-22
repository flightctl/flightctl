const { defineConfig } = require("cypress");

module.exports = defineConfig({
  e2e: {
    setupNodeEvents(on, config) {
      // implement node event listeners here
    },
    supportFile: false,
    specPattern: 'e2e/*.cy.{js,jsx,ts,tsx}'
  },
  env: {
    host: 'https://console-openshift-console.apps.ocp-edge-cluster-0.qe.lab.redhat.com',
    auth: 'https://oauth-openshift.apps.ocp-edge-cluster-0.qe.lab.redhat.com',
    username: 'kube:admin',
    password: 'Dtfw2-z9s49-YPfam-etIFK'
  },
});

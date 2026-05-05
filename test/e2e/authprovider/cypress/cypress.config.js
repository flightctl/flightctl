const { defineConfig } = require('cypress')

module.exports = defineConfig({
  chromeWebSecurity: false,
  e2e: {
    specPattern: 'e2e/**/*.cy.js',
    supportFile: false,
    setupNodeEvents(on, config) {
      config.env.authProviderAuthorizeUrl =
        process.env.CYPRESS_AUTHPROVIDER_AUTHORIZE_URL || config.env.authProviderAuthorizeUrl || ''
      config.env.authProviderCallbackPort =
        process.env.CYPRESS_AUTHPROVIDER_CALLBACK_PORT || config.env.authProviderCallbackPort || '8080'
      config.env.authProviderUI =
        process.env.CYPRESS_AUTHPROVIDER_UI || config.env.authProviderUI || ''
      config.env.authProviderUsername =
        process.env.CYPRESS_AUTHPROVIDER_USERNAME || config.env.authProviderUsername || ''
      config.env.authProviderPassword =
        process.env.CYPRESS_AUTHPROVIDER_PASSWORD || config.env.authProviderPassword || ''
      return config
    },
  },
  video: false,
  screenshotOnRunFailure: true,
})

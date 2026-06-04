const resolveAuthorizeUrl = () => String(Cypress.env('authProviderAuthorizeUrl') || '').trim()
const providerUI = () => String(Cypress.env('authProviderUI') || '').trim().toLowerCase()
const callbackPort = () => String(Cypress.env('authProviderCallbackPort') || '8080').trim()
const username = () => String(Cypress.env('authProviderUsername') || '').trim()
const password = () => String(Cypress.env('authProviderPassword') || '').trim()
const callbackLocationFragment = () => `localhost:${callbackPort()}`

describe('Auth provider browser login', () => {
  it('authenticates through the configured provider UI', () => {
    const authorizeUrl = resolveAuthorizeUrl()
    expect(authorizeUrl, 'CYPRESS_AUTHPROVIDER_AUTHORIZE_URL must be set').not.to.eq('')
    expect(providerUI(), 'CYPRESS_AUTHPROVIDER_UI must be set').not.to.eq('')
    expect(username(), 'CYPRESS_AUTHPROVIDER_USERNAME must be set').not.to.eq('')
    expect(password(), 'CYPRESS_AUTHPROVIDER_PASSWORD must be set').not.to.eq('')

    cy.visit(authorizeUrl, { timeout: 120000, retryOnStatusCodeFailure: true })

    switch (providerUI()) {
      case 'keycloak':
        completeKeycloakLogin()
        break
      case 'pam':
        completePamLogin()
        break
      case 'openshift':
        completeOpenShiftLogin()
        break
      case 'aap':
        completeAAPLogin()
        break
      default:
        throw new Error(`unsupported auth provider UI: ${providerUI()}`)
    }

    cy.url({ timeout: 180000 }).should('include', `localhost:${callbackPort()}`)
  })
})

function completeKeycloakLogin() {
  cy.get('#username', { timeout: 120000 }).should('be.visible').clear().type(username())
  cy.get('#password').should('be.visible').clear().type(password(), { log: false })
  cy.get('#kc-login').should('be.visible').click()
}

function completePamLogin() {
  cy.get('#username', { timeout: 120000 }).should('be.visible').clear().type(username())
  cy.get('#password').should('be.visible').clear().type(password(), { log: false })
  cy.get('button[type="submit"]').contains(/log in/i).should('be.visible').click()
}

function completeOpenShiftLogin() {
  cy.get('body', { timeout: 120000 }).then(($body) => {
    const text = $body.text() || ''
    const hasLoginForm = $body.find('#inputUsername').length > 0
    if (!hasLoginForm && /kube:admin/i.test(text)) {
      cy.contains('button, a, span', /kube:admin/i, { timeout: 120000 }).click({ force: true })
      return false
    }

    return hasLoginForm
  }).then((shouldSubmitCredentials) => {
    if (!shouldSubmitCredentials) {
      return
    }

    cy.get('#inputUsername', { timeout: 120000 }).should('be.visible').clear().type(username())
    cy.get('#inputPassword').should('be.visible').clear().type(password(), { log: false })
    cy.get('#co-login-button').should('be.visible').click()
  })
}

function completeAAPLogin() {
  cy.document({ timeout: 120000 }).should((doc) => {
    if (isCallbackDocument(doc)) {
      return
    }

    expect(detectAAPLoginForm(doc.body), 'AAP login form').to.not.be.null
  }).then((doc) => {
    if (isCallbackDocument(doc)) {
      return
    }

    submitUsernamePasswordForm(detectAAPLoginForm(doc.body))
  })
}

function isCallbackDocument(doc) {
  const href = String(doc.location && doc.location.href ? doc.location.href : '')
  return href.includes(callbackLocationFragment())
}

function submitUsernamePasswordForm(form) {
  cy.get(form.usernameSelector, { timeout: 120000 }).should('be.visible').clear().type(username())
  cy.get(form.passwordSelector).should('be.visible').clear().type(password(), { log: false })
  cy.get(form.submitSelector).first().should('be.visible').click({ force: true })
}

function detectAAPLoginForm(body) {
  const selectors = [
    {
      usernameSelector: '#username',
      passwordSelector: '#password',
      submitSelector: 'button[type="submit"]',
    },
    {
      usernameSelector: 'input[name="username"]',
      passwordSelector: 'input[name="password"]',
      submitSelector: 'button[type="submit"]',
    },
    {
      usernameSelector: 'input[type="text"]',
      passwordSelector: 'input[type="password"]',
      submitSelector: 'button[type="submit"]',
    },
    {
      usernameSelector: 'input[type="email"]',
      passwordSelector: 'input[type="password"]',
      submitSelector: 'button[type="submit"]',
    },
  ]

  for (const candidate of selectors) {
    if (
      body.querySelector(candidate.usernameSelector) &&
      body.querySelector(candidate.passwordSelector) &&
      body.querySelector(candidate.submitSelector)
    ) {
      return candidate
    }
  }

  return null
}

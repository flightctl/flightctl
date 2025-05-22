describe('Fleet Management', () => {
    it('Should create a fleet', () => {
        cy.login(`${Cypress.env('host')}`, `${Cypress.env('auth')}`, `${Cypress.env('username')}`, `${Cypress.env('password')}`)
        cy.waitForPageLoad()
        cy.get('.pf-v5-c-modal-box__close > .pf-v5-c-button').should('be.visible')
        cy.get('.pf-v5-c-modal-box__close > .pf-v5-c-button').click()
        cy.createFleet()
    })
})
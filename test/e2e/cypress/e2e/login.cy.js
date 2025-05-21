describe('template spec', () => {
    it('passes', () => {
        cy.visit('https://console-openshift-console.apps.ocp-edge-cluster-0.qe.lab.redhat.com')
        cy.wait(7000)
        cy.origin('https://oauth-openshift.apps.ocp-edge-cluster-0.qe.lab.redhat.com', () => {

            cy.contains('kube:admin').click()
            cy.get('#inputUsername').should('exist')
            cy.get('#inputUsername').should('be.visible')
            cy.get('#inputPassword').should('exist')
            cy.get('#inputPassword').should('be.visible')
            cy.get('#inputUsername').type('kubeadmin')
            cy.get('#inputPassword').type('Dtfw2-z9s49-YPfam-etIFK')
            cy.contains('button', 'Log in').click()
        })
        cy.wait(15000)
        cy.get('.pf-v5-c-modal-box__close > .pf-v5-c-button').should('be.visible')
        cy.get('.pf-v5-c-modal-box__close > .pf-v5-c-button').click()
    })
})
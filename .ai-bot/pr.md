## Summary

**IMPORTANT: This fix targets the `flightctl-ui` repository, not this repository.**

The vertical separator bar between the filter sidebar and catalog items grid in the Software Catalog page does not extend to the bottom of the page. This is a CSS layout issue in the `flightctl-ui` repository where the flex chain from the page root to the `Split` container is broken — no intermediate container uses `flex: 1` to fill remaining vertical space.

### Fix (in `flightctl-ui`)

- Added `isFilled` and `className` props to `ListPage` component to allow callers to control the `PageSection` growth behavior
- Added flex layout CSS classes to the Catalog page container chain (`fctl-catalog-page__list-page`, `__content`, `__body`, `__split`) that propagate `flex: 1` from the page section down to the `Split` component
- The `Divider` then stretches to the full height of the `Split` via default `align-items: stretch`

### Files changed (in `flightctl-ui` repo)
- `libs/ui-components/src/components/ListPage/ListPage.tsx` — added optional `isFilled` and `className` props
- `libs/ui-components/src/components/Catalog/CatalogPage.tsx` — applied `isFilled` and CSS classes
- `libs/ui-components/src/components/Catalog/CatalogPage.css` — added flex chain styles

### Patch

The complete diff is available in `.ai-bot/patch.diff`.

## Test plan

- [ ] Open Software Catalog page and verify the vertical separator extends to the bottom
- [ ] Resize browser — bar should always reach the bottom regardless of viewport height
- [ ] Verify landing page (empty catalog) is unaffected
- [ ] Verify other ListPage-based pages (Fleets, Devices, Repositories) are unaffected
- [ ] Cross-browser check (Chrome, Firefox, Safari)

Fixes EDM-4859

# Implementation Notes — EDM-4859

## Repository

**This fix targets the `flightctl-ui` repository** (https://github.com/flightctl/flightctl-ui), not the `flightctl` Go backend repository where this task was assigned.

## Files Modified

### 1. `libs/ui-components/src/components/ListPage/ListPage.tsx`
- **What:** Added optional `isFilled` and `className` props to `ListPageProps`, passed through to the underlying `PageSection`.
- **Why:** The `ListPage` component wraps its content in a `PageSection` but didn't expose PatternFly's `isFilled` prop, preventing callers from making the page section grow to fill available space in the page's flex layout.
- **Impact:** Backward-compatible — both props are optional and default to `undefined`, preserving existing behavior for all other pages using `ListPage`.

### 2. `libs/ui-components/src/components/Catalog/CatalogPage.tsx`
- **Line ~455:** Added `isFilled` and `className="fctl-catalog-page__list-page"` to the `<ListPage>` call in `CatalogPage`.
  - **Why:** `isFilled` adds `flex-grow: 1` to the outer `PageSection` so it fills remaining page height. The className enables the flex column layout via CSS.
- **Line ~218:** Added `className="fctl-catalog-page__content"` to the wrapper `<div>` in `CatalogPageContent`.
  - **Why:** This div wraps the toolbar and page body; it needs to be a flex column container that grows to fill its parent.
- **Line ~237:** Added `className="fctl-catalog-page__body"` to the inner `<PageSection>`.
  - **Why:** This section wraps the `Split` layout and needs to display as flex and grow.
- **Line ~238:** Added `className="fctl-catalog-page__split"` to the `<Split>` component.
  - **Why:** The `Split` needs `flex: 1` to fill the inner `PageSection`, making the `Divider` stretch to the full height.

### 3. `libs/ui-components/src/components/Catalog/CatalogPage.css`
Added four CSS rules to establish the flex chain from the page section to the Split:

```css
.fctl-catalog-page__list-page {
  display: flex;
  flex-direction: column;
}

.fctl-catalog-page__content {
  display: flex;
  flex-direction: column;
  flex: 1;
}

.fctl-catalog-page__body {
  display: flex;
  flex: 1;
}

.fctl-catalog-page__split {
  flex: 1;
}
```

## Design Choices

### Approach taken: Flex chain propagation
The fix establishes a complete flex chain from the page's main area (already a flex column) through to the `Split` container. Each intermediate container gets `display: flex; flex-direction: column; flex: 1`, allowing the `Split` (and thus the `Divider`) to fill the remaining viewport height.

### Alternatives considered

1. **Using `min-height: calc(100vh - Xpx)` on the Split** — Rejected because hardcoding pixel offsets for the header height is fragile and breaks when the header height changes or on different screen configurations.

2. **Restructuring CatalogPage to not use ListPage** — Rejected to avoid duplicating the ListPage pattern and maintain consistency with other pages.

3. **Making ListPage always use `isFilled`** — Rejected because it would change the layout behavior of all pages using ListPage, which is an unnecessary blast radius.

4. **Using CSS Grid instead of flex** — Rejected because the existing PatternFly layout system is flex-based and a grid approach would fight the framework.

## Test Strategy

This is a CSS-only layout fix. No unit tests were added because:
- The change is purely visual (flex properties on containers)
- No behavioral logic changed
- The project doesn't have visual regression testing infrastructure
- The fix can be verified by opening the Software Catalog page and confirming the vertical separator extends to the bottom of the viewport

### Manual verification steps:
1. Open the Software Catalog page
2. Confirm the vertical separator bar extends from top to bottom of the content area
3. Resize the browser window to various heights — the bar should always reach the bottom
4. With few catalog items, the bar should still reach the bottom
5. With many catalog items (scrolling), the bar should extend the full height of the content
6. Check the landing page view (empty catalog) — it should be unaffected

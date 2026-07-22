# Root Cause Analysis — EDM-4859

## Summary

The vertical separator bar (PatternFly `Divider` with `orientation="vertical"`) in the Software Catalog page does not extend to the bottom of the page because its parent `Split` flex container does not fill the remaining viewport height.

## Root Cause

The `Divider` component renders as an `<hr>` element inside a PatternFly `Split` layout (which uses `display: flex; flex-direction: row`). By default, flex items stretch to match the tallest sibling (`align-items: stretch`). The divider does stretch — but only to the height of the tallest sibling (either the filter sidebar or the catalog items grid), which is shorter than the page.

The underlying issue is that no container in the DOM chain between the page root and the `Split` uses `flex: 1` to fill remaining vertical space. The layout chain is:

```
<main class="pf-v6-c-page__main">          ← flex column (page layout)
  <section class="pf-v6-c-page__main-section">  ← ListPage's PageSection (NO flex-grow)
    <Stack> (title + description)
    <div>                                    ← wrapper div (block, no flex)
      <CatalogPageToolbar />
      <section class="pf-v6-c-page__main-section pf-m-wizard">  ← inner PageSection (block, no flex)
        <Split>                              ← flex row, height = content
          <SplitItem> (filters)
          <Divider orientation="vertical" />  ← stretches to tallest sibling only
          <SplitItem isFilled> (catalog items)
```

Since the outer `PageSection` doesn't grow (`isFilled` not set) and the intermediate containers are block-level, the `Split` only takes up as much height as its content requires.

## Affected Component

**Repository:** `flightctl-ui` (https://github.com/flightctl/flightctl-ui)  
**Files:**
- `libs/ui-components/src/components/Catalog/CatalogPage.tsx`
- `libs/ui-components/src/components/Catalog/CatalogPage.css`
- `libs/ui-components/src/components/ListPage/ListPage.tsx`

**Note:** This issue is in the `flightctl-ui` repository, not the `flightctl` Go backend repository.

## Confidence

95% — The layout structure is straightforward and the flex chain gap is clearly visible in the component hierarchy.

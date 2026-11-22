// Package firmware obtains and authenticates approved firmware updater code.
// It executes only verified, policy-approved updater payloads through the
// bounded phase runner, and never executes unverified or unconstrained image
// content.
package firmware

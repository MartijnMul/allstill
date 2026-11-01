// SPDX-License-Identifier: AGPL-3.0-only

// Package api holds what the server and clients in this repository share of the
// API description (api/openapi.yaml): its types and server interface, generated
// from the Probe part for now, and the probe's protocol version.
package api

// ProbeProtocolVersion is the protocol a probe and the server speak. A change
// a probe of the last version could not follow raises it, and the server then
// refuses that probe with what to update.
const ProbeProtocolVersion = 1

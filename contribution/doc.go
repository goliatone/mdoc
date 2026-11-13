// Package contribution prepares and performs explicit Google Docs contributions.
//
// Hosts own authorization and a durable, shared operation ledger. Atomically bind
// (namespace, operation ID, account binding) to the exact authorized plan/digest;
// persist a sending claim before calling a mutation and persist every Outcome,
// including when error is non-nil. Serialize claims across all host processes.
// Cached committed receipts return without dispatch. A crash in sending or an
// unknown result requires read-only inspection/manual reconciliation, never a
// retry. Attempt IDs and digests alone provide neither authorization nor durable
// idempotency. Separate ledgers cannot safely coordinate the same operation.
//
// Plans include private document content, not portable context or credentials.
// An authenticated client/transport is trusted host code and must not retry
// mutations internally. No method opens a browser or selects ambient credentials.
package contribution

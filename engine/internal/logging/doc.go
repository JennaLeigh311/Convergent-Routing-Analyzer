// Package logging is the engine's structured-logging foundation: a thin wrapper
// over log/slog so every binary configures logs identically and future
// components emit structured, queryable logs instead of ad-hoc prints.
//
// Typical use at process start:
//
//	logger := logging.Setup()                  // configured from the environment
//	logger = logger.With("service", "routing-server")
//	logger.Info("starting", "addr", addr)
//
// Setup also installs the logger as the slog default, so code that logs via
// slog.Info/Warn/etc. inherits the same configuration.
//
// Convention: entrypoints (cmd/*) hold the explicit *slog.Logger returned by
// Setup; library and leaf packages take an injected *slog.Logger or use
// slog.Default(), and never construct their own. Never log secrets, credentials,
// full DSNs, or API keys, and treat raw GPS coordinates as user-location PII —
// log only derived values (segment IDs, counts), not raw lat/lon tied to a device.
package logging

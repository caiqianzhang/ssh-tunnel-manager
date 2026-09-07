# Reference implementations

This directory holds reference implementations of the Baidu Cloud DNS
(BCE Auth V1) integration that are **not built by this project**. They
exist as cross-checks for the live Go code:

- `baidu_dns.rs` — Rust reference port. The Go `internal/bcd` package
  (`CanonicalURI`, `SignRequest`, `LoadCredentials`) was ported from this
  file and its signing vector is cross-checked against it in
  `baidu_dns_test.go`. There is no Rust toolchain in this repo; the file
  is kept as the historical source of truth for the signature vector.

The live implementation lives in `internal/bcd/`; the app's Baidu DNS
client wraps it (`baidu_dns.go`).
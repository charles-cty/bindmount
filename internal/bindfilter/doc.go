// Package bindfilter wraps the undocumented Bind Filter (bindflt.sys) user-mode
// API in bindfltapi.dll. See docs/BindFilterAPI.md for the research behind the
// declarations. The public Bindlink API should be preferred when its semantics
// suffice; this package exists because Bindlink exposes no enumeration and no
// silo scope.
package bindfilter

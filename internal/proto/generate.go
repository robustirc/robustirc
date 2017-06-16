// Empty file containing instructions for “go generate” on how to rebuild the
// capnproto generated code.
package proto

//go:generate protoc types.proto snapshot.proto --gofast_out=.

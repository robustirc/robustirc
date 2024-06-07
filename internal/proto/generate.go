// Empty file containing instructions for “go generate” on how to rebuild the
// Go Protobuf generated code.
package proto

//go:generate protoc types.proto snapshot.proto --go_out=. --go_opt=paths=source_relative

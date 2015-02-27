#!/bin/sh
# vim:ts=4:sw=4:et
# Runs go test with -coverprofile, but specifying all packages as -coverpkg,
# and merges all the resulting coverage profiles into one big profile.

INTERMEDIATE=$(mktemp)
RESULT=$(mktemp)
echo "mode: count" > "$RESULT"
PACKAGES=$(go list github.com/robustirc/robustirc/... | grep -v /cmd/ | grep -v mod_test | grep -v util | grep -v localnet)
for pkg in $PACKAGES; do
    go test -covermode=count -coverprofile="$INTERMEDIATE" -coverpkg=$(echo $PACKAGES | tr ' ' ',') $pkg
    go run cmd/robustirc-merge-coverage/coverage.go -input="$INTERMEDIATE,$RESULT" -output="$RESULT"
done
rm "$INTERMEDIATE"
go tool cover -html="$RESULT"

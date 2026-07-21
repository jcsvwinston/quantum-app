# quantum-app — build and verification entry points.
# GOWORK=off everywhere: the suite resolves from the module proxy at the
# certified tags in go.mod, never from a local workspace.
export GOWORK := off

.PHONY: build vet test e2e-local guard manifest-check manifest-gen guard-fixtures clean

build:
	go build ./...
	go build -o bin/quantum-app ./cmd/quantum-app

vet:
	go vet ./...

test:
	go test ./...

guard:
	./scripts/check_no_workspace.sh

# Full local E2E: brings the Docker services up, runs the E2E suite against a
# real app binary, and tears the services down.
e2e-local:
	./scripts/e2e_local.sh

manifest-gen:
	./scripts/gen_suite_manifest.sh

manifest-check:
	./scripts/check_suite_manifest.sh

# Executes every gate's negative fixture: each gate must BITE on a doctored
# tree with the expected cause (guard-of-guards style; CI runs this too).
guard-fixtures:
	./scripts/guard_fixtures.sh

clean:
	rm -rf bin

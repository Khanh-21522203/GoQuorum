# GoQuorum v2 — workspace Makefile
# All commands run across every module via the go.work workspace.
# NB: v2/ is not itself a module, so the relative pattern ./... matches nothing.
# The module-path prefix goquorum.io/v2/... is what spans all workspace modules.

PKGS := goquorum.io/v2/...

.PHONY: build vet test fmt tidy clean tree

build:
	go build $(PKGS)

vet:
	go vet $(PKGS)

test:
	go test $(PKGS)

fmt:
	gofmt -l -w .

# Run `go mod tidy` in each module (needed once real external deps are introduced).
tidy:
	@for m in contracts engine infra gateway client server cli test; do \
		echo "==> tidy $$m"; (cd $$m && go mod tidy) || exit 1; \
	done

tree:
	@find . -name '*.go' -o -name 'go.mod' -o -name 'go.work' | sort

clean:
	go clean ./...

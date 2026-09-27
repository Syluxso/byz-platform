MODULES := . ginx notify files directory

.PHONY: test vet lint tidy ci

test:
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done

vet:
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go vet ./...) || exit 1; done

lint:
	golangci-lint run ./...

tidy:
	@for m in $(MODULES); do (cd $$m && go mod tidy) || exit 1; done

ci: vet test lint

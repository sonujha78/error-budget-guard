.RECIPEPREFIX = >
BIN := bin/ebg

.PHONY: build test vet fmt run clean

build:
> go build -o $(BIN) ./cmd/ebg

test:
> go test ./... -count=1

vet:
> go vet ./...

fmt:
> gofmt -l -w .

run: build
> ./$(BIN) version

clean:
> rm -rf bin

.PHONY: verify reports
verify: build
> ./$(BIN) verify scenarios/*

reports: build
> ./scripts/make-reports.sh

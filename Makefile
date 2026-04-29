IMAGE ?= localhost:5001/schedulab-scheduler:latest

.PHONY: build
build:
	go build -o bin/schedulab-scheduler ./cmd/scheduler

.PHONY: image
image:
	docker build -t $(IMAGE) .

.PHONY: fmt
fmt:
	gofmt -w cmd pkg

.PHONY: tidy
tidy:
	go mod tidy


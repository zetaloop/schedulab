FROM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/schedulab-scheduler ./cmd/scheduler

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/schedulab-scheduler /usr/local/bin/schedulab-scheduler

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/schedulab-scheduler"]


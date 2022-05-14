FROM golang:1.17 AS builder
WORKDIR /go/src/github.com/stolostron/singapore
COPY . .
ENV GO_PACKAGE github.com/stolostron/singapore

RUN make build --warn-undefined-variables

FROM registry.access.redhat.com/ubi8/ubi-minimal:latest

COPY --from=builder /go/src/github.com/stolostron/singapore/kcp-ocm /
RUN microdnf update && microdnf clean all
USER 65532:65532

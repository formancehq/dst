# Compiler
FROM golang:1.24-trixie AS compiler

RUN apt-get update && apt-get install -y bash git gcc

RUN go install github.com/antithesishq/antithesis-sdk-go/tools/antithesis-go-instrumentor@latest

COPY go.* /src/.
COPY bin /src/bin
COPY internal /src/internal

RUN mkdir /instrumented
RUN ls .
WORKDIR /src
RUN antithesis-go-instrumentor . /instrumented
WORKDIR /instrumented/customer

RUN go build -o /init ./bin/init
RUN mkdir -p /cmds
RUN for file in $(ls ./bin/cmds/); do cp ./github.com_V_formancehq_V_dst_V_workload_antithesis_catalog.go ./bin/cmds/$file; go build -race -o /cmds/$file ./bin/cmds/$file; done

# Runner
FROM debian:trixie

RUN apt-get update && apt-get install -y curl

ARG LEDGER_LATEST_TAG

COPY --from=compiler /init /init
ENTRYPOINT ["/init"]

COPY --from=compiler /cmds/* /opt/antithesis/test/v1/main/
COPY --from=compiler /instrumented/symbols/ /symbols

RUN echo -n ${LEDGER_LATEST_TAG} > /ledger_latest_tag

# Compiler
FROM golang:1.24-trixie AS compiler

RUN apt-get update && apt-get install -y bash git gcc

RUN go install github.com/antithesishq/antithesis-sdk-go/tools/antithesis-go-instrumentor@latest

COPY go.* /src/.
COPY bin /src/bin
COPY internal /src/internal

RUN mkdir /instrumented
WORKDIR /src
RUN antithesis-go-instrumentor . /instrumented
WORKDIR /instrumented/customer

RUN go build -o /init ./bin/init
RUN mkdir -p /cmds
# Build each command into /cmds/<template>/<command>, preserving the
# bin/cmds/<template>/ layout so each lands in its own test template.
RUN for cmd in $(find ./bin/cmds -mindepth 2 -maxdepth 2 -type d | sed 's|^\./bin/cmds/||'); do \
		cp ./github.com_V_formancehq_V_dst_V_workload_antithesis_catalog.go "./bin/cmds/$cmd/"; \
		mkdir -p "/cmds/$(dirname "$cmd")"; \
		go build -race -o "/cmds/$cmd" "./bin/cmds/$cmd"; \
	done

# Runner
FROM debian:trixie

RUN apt-get update && apt-get install -y curl

ARG LEDGER_LATEST_TAG

COPY --from=compiler /init /init
ENTRYPOINT ["/init"]

# Each bin/cmds/<template>/ directory becomes an Antithesis test template under
# test/v1/. Antithesis runs exactly one template per execution, so the model
# driver (model) — which drives the system itself and must own the whole
# timeline — stays isolated from the rest of the suite (main).
COPY --from=compiler /cmds/ /opt/antithesis/test/v1/
COPY --from=compiler /instrumented/symbols/ /symbols

RUN echo -n ${LEDGER_LATEST_TAG} > /ledger_latest_tag

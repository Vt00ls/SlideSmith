ARG GO_IMAGE=golang:1.25-alpine
ARG RUNTIME_IMAGE=docker:27-cli

FROM ${GO_IMAGE} AS build

ARG TARGET=server
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}

WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/slidesmith ./cmd/${TARGET}

FROM ${RUNTIME_IMAGE}

WORKDIR /app
COPY --from=build /out/slidesmith /app/slidesmith
COPY deploy/server/agent-compose-docker-cli.sh /app/deploy/server/agent-compose-docker-cli.sh
RUN chmod 0755 /app/deploy/server/agent-compose-docker-cli.sh
COPY runtime/ppt-master-agent/scripts /app/runtime/ppt-master-agent/scripts
COPY runtime/ppt-master-agent/workflows /app/runtime/ppt-master-agent/workflows

ENV SLIDESMITH_HTTP_ADDR=0.0.0.0:18080
ENV SLIDESMITH_GIN_MODE=release
ENV SLIDESMITH_AGENT_COMPOSE_CLI=/app/deploy/server/agent-compose-docker-cli.sh
ENV SLIDESMITH_AGENT_COMPOSE_WORKDIR=/app/runtime/ppt-master-agent

EXPOSE 18080

ENTRYPOINT ["/app/slidesmith"]

# rbac-go: minimal on-prem RBAC v1 (replaces insights-rbac where only
# koku + koku-service-operator need it). Pure Go (no cgo), static binary.
FROM docker.io/library/golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/rbac-go ./cmd/rbac-go

FROM gcr.io/distroless/base-debian12
WORKDIR /data
COPY --from=build /out/rbac-go /rbac-go
EXPOSE 8080
ENTRYPOINT ["/rbac-go", "serve", "--addr", ":8080"]

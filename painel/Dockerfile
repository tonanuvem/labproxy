FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
# Sem dependencias externas: nao ha "go mod download" a fazer.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /painel .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /painel /painel
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/painel"]

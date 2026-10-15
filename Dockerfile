FROM golang:1.27-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/qq-tavern ./cmd/qq-tavern && mkdir /out/data && chown 10001:10001 /out/data && apk info -vv ca-certificates ca-certificates-bundle > /out/build-packages.txt

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/qq-tavern /qq-tavern
COPY --from=build --chown=10001:10001 /out/data /data
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/
COPY third_party/licenses /licenses/third_party/
COPY --from=build /out/build-packages.txt /licenses/build-packages.txt
USER 10001:10001
ENTRYPOINT ["/qq-tavern"]

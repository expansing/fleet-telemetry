FROM golang:1.26-bookworm AS build

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    wget \
    ca-certificates \
    libssl-dev \
    protobuf-compiler \
    libprotobuf-dev \
    libsodium-dev \
    libzmq3-dev \
    libstdc++-12-dev \
    && rm -rf /var/lib/apt/lists/*


WORKDIR /build

RUN wget https://github.com/edenhill/librdkafka/archive/refs/tags/v2.3.0.tar.gz \
    && tar -xzvf v2.3.0.tar.gz

WORKDIR /build/librdkafka-2.3.0

RUN ./configure --enable-static --disable-shared \
    && make -j$(nproc) \
    && make install


WORKDIR /go/src/fleet-telemetry

COPY . .

ENV CGO_ENABLED=1
ENV CGO_LDFLAGS="-lstdc++ -Wl,-rpath,/usr/local/lib"
ENV PKG_CONFIG_PATH=/usr/lib/pkgconfig:/usr/local/lib/pkgconfig


RUN make


FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    librdkafka1 \
    libzmq5 \
    libsodium23 \
    libpgm-5.3-0 \
    libnorm1 \
    libbsd0 \
    libmd0 \
    libkrb5-3 \
    libgssapi-krb5-2 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*


WORKDIR /

COPY --from=build /go/bin/fleet-telemetry /


CMD ["/fleet-telemetry", "-config", "/etc/fleet-telemetry/config.json"]
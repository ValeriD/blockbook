FROM gostartups/golang-rocksdb-zeromq:andromeda

WORKDIR /home
# Build blockbook
COPY . /home/blockbook

WORKDIR /home/blockbook
RUN go mod download
RUN go mod tidy
RUN go build -tags rocksdb_6_16

ENV RPC_USER=user
ENV RPC_PASS=pass
ENV RPC_PORT=
ENV RPC_HOST=hydra.nodes.changex.io
ENV BLOCKBOOK_PORT=9197


RUN ./contrib/scripts/build-blockchaincfg.sh hydra

COPY run.sh run.sh

EXPOSE 9197

ENTRYPOINT sh run.sh 
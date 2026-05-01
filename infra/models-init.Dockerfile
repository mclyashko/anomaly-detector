FROM minio/mc

COPY infra/models/ /models/
COPY scripts/models-init.sh /models-init.sh

ENTRYPOINT ["/bin/sh", "-c"]
CMD ["sh", "/models-init.sh"]

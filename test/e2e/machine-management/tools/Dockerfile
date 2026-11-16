FROM python:3.13.9-slim-bookworm

RUN python -m pip install --no-cache-dir \
    redfish==3.4.2 \
    redfish-service-validator==3.1.8 \
    redfish-interop-validator==3.0.3

WORKDIR /work
ENTRYPOINT ["/bin/sh", "-c"]

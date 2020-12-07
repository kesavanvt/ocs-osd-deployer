FROM registry.access.redhat.com/ubi8/ubi-minimal

COPY k8s.repo /etc/yum.repos.d/k8s.repo
RUN microdnf update -y && \
    microdnf install -y \
      bash \
      kubectl \
    && curl -L https://github.com/mikefarah/yq/releases/download/3.4.1/yq_linux_amd64 -o /bin/yq \
    && chmod +x /bin/yq \
    && microdnf clean all && \
    rm -rf /var/cache/yum

COPY deploy.sh /
COPY storagecluster.yml /

RUN chmod 755 /deploy.sh && \
    chmod 644 /storagecluster.yml

ARG builddate="(unknown)"
ARG version="(unknown)"

LABEL org.label-schema.build-date="${builddate}" \
      org.label-schema.description="Pod to deploy the initial StorageCluster yaml for OCS" \
      org.label-schema.license="Apache-2.0" \
      org.label-schema.name="OCS on OSD deployer" \
      org.label-schema.schema-version="1.0" \
      org.label-schema.vcs-ref="${version}" \
      org.label-schema.vcs-url="https://github.com/johnstrunk/ocs-osd-deployer" \
      org.label-schema.vendor="Red Hat" \
      org.label-schema.version="${version}"

ENTRYPOINT [ "/deploy.sh" ]

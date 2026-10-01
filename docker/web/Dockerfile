FROM nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10
# A template: the image's entrypoint renders it into conf.d at start, substituting API_UPSTREAM
# only (NGINX_ENVSUBST_FILTER), so nginx's own $host and $uri stay as they are. On Umbrel the
# compose sets the api container's unique name; elsewhere the default service name applies.
ENV API_UPSTREAM=api:8080 NGINX_ENVSUBST_FILTER=^API_UPSTREAM$
COPY docker/web/nginx.conf /etc/nginx/templates/default.conf.template
# One worker: "auto" starts one per CPU (33 processes on a 32-thread machine) to serve one person.
RUN sed -i 's/^worker_processes .*/worker_processes 1;/' /etc/nginx/nginx.conf \
 && grep -q '^worker_processes 1;' /etc/nginx/nginx.conf
COPY web/dist /usr/share/nginx/html

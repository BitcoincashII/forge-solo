# nginx 1.30, the stable branch: 1.27 reached its end, with fixes no longer made for it.
FROM nginx:1.30.5-alpine@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94
# A template: the image's entrypoint renders it into conf.d at start, substituting API_UPSTREAM
# only (NGINX_ENVSUBST_FILTER), so nginx's own $host and $uri stay as they are. On Umbrel the
# compose sets the api container's unique name; elsewhere the default service name applies.
ENV API_UPSTREAM=api:8080 NGINX_ENVSUBST_FILTER=^API_UPSTREAM$
COPY docker/web/nginx.conf /etc/nginx/templates/default.conf.template
COPY docker/web/security-headers.conf /etc/nginx/snippets/security-headers.conf
# One worker: "auto" starts one per CPU (33 processes on a 32-thread machine) to serve one person.
RUN sed -i 's/^worker_processes .*/worker_processes 1;/' /etc/nginx/nginx.conf \
 && grep -q '^worker_processes 1;' /etc/nginx/nginx.conf
COPY web/dist /usr/share/nginx/html

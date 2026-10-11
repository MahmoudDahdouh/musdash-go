FROM busybox:1.36
ARG GREETING
RUN mkdir -p /www && echo "$GREETING from the api" > /www/index.html
CMD ["httpd", "-f", "-p", "3000", "-h", "/www"]

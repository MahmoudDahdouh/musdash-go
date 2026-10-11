# A small app: one page, served on port 8080.
FROM busybox:1.36
COPY site /www
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]

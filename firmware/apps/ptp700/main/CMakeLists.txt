idf_component_register(SRCS "main.c" "ptp700.c" "ptp700_proto.c" INCLUDE_DIRS "." EMBED_FILES "sample.rows"
                       PRIV_REQUIRES board esp_timer)

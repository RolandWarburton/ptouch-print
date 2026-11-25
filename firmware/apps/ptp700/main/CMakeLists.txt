idf_component_register(SRCS "main.c" "ptp700.c" "ptp700_proto.c" INCLUDE_DIRS "." EMBED_FILES "test24.rows"
                       PRIV_REQUIRES board usb_printer esp_timer)

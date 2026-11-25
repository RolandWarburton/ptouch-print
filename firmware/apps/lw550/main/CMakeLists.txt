idf_component_register(SRCS "main.c" "dymo550.c" "dymo550_proto.c" INCLUDE_DIRS "." EMBED_FILES "sample.rows"
                       PRIV_REQUIRES board diecut)

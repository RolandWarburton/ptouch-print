idf_component_register(SRCS "board.c" "net.c" "poller.c" "proto.c" "settings.c" INCLUDE_DIRS "include"
                       REQUIRES usb_printer
                       PRIV_REQUIRES esp_app_format esp_event esp_http_client esp_netif esp_timer esp_wifi json mbedtls nvs_flash)

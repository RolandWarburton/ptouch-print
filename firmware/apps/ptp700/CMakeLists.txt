cmake_minimum_required(VERSION 3.16)
set(EXTRA_COMPONENT_DIRS ../../components)
set(SDKCONFIG_DEFAULTS "${CMAKE_CURRENT_LIST_DIR}/../../sdkconfig.defaults")
# Only what the app needs. esp_psram is named because nothing requires it: without it
# the SPIRAM settings are dropped with no error.
set(COMPONENTS main esp_psram)
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(ptp700)

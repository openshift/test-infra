load("@io_bazel_rules_go//go:def.bzl", "go_binary", "go_library", "go_test")

go_library(
    name = "go_default_library",
    srcs = [
        "doc.go",
        "main.go",
        "server.go",
    ],
    importpath = "k8s.io/test-infra/experiment/vendor-syncer",
    visibility = ["//visibility:private"],
    deps = [
        "//prow/git:go_default_library",
        "//prow/github:go_default_library",
        "//prow/hook:go_default_library",
        "//vendor/github.com/sirupsen/logrus:go_default_library",
        "//vendor/gopkg.in/yaml.v2:go_default_library",
    ],
)

go_binary(
    name = "vendor-syncer",
    importpath = "k8s.io/test-infra/experiment/vendor-syncer",
    library = ":go_default_library",
    visibility = ["//visibility:public"],
)

go_test(
    name = "go_default_test",
    srcs = ["server_test.go"],
    importpath = "k8s.io/test-infra/experiment/vendor-syncer",
    library = ":go_default_library",
    deps = ["//prow/github:go_default_library"],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [":package-srcs"],
    tags = ["automanaged"],
    visibility = ["//visibility:public"],
)

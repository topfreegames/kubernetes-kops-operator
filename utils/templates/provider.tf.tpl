provider "aws" {
    region = "us-east-1"
}

provider "aws" {
    alias  = "files"
    region = "us-east-1"
}
FROM microsoft/nanoserver:1709

RUN mkdir C:\temp\test & echo hello > C:\temp\test\hello

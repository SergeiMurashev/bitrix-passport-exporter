package main

// @title Bitrix Passport Exporter API
// @version 1.2.2
// @description API сервиса выгрузки "Паспорта проекта" и задач из Bitrix24.
// @description
// @description Базовый формат JSON-ответов:
// @description - ok
// @description - status
// @description - message
// @description - data
// @description - error
// @description - meta
// @BasePath /
// @schemes http https
//
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-API-Key
//
// @securityDefinitions.apikey CookieAuth
// @in cookie
// @name bp_session
//
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
